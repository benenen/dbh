package etcd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseDSN(t *testing.T) {
	cfg, err := parseDSN("etcd://alice:secret@127.0.0.1:2379?tls=true&timeout=5s")
	if err != nil {
		t.Fatalf("parseDSN failed: %v", err)
	}
	if cfg.baseURL != "https://127.0.0.1:2379" {
		t.Fatalf("unexpected baseURL: %s", cfg.baseURL)
	}
	if cfg.user != "alice" || cfg.pass != "secret" {
		t.Fatalf("unexpected credentials: %s / %s", cfg.user, cfg.pass)
	}
	if cfg.timeout.Seconds() != 5 {
		t.Fatalf("unexpected timeout: %v", cfg.timeout)
	}
}

func TestEtcdDriverOperations(t *testing.T) {
	store := map[string]string{
		"/app/name": "dbh",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		switch r.URL.Path {
		case "/v3/maintenance/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version":   "3.5.17",
				"dbSize":    "20480",
				"leader":    "1",
				"raftIndex": "10",
				"raftTerm":  "2",
			})
		case "/v3/kv/put":
			k, _ := base64.StdEncoding.DecodeString(body["key"].(string))
			v, _ := base64.StdEncoding.DecodeString(body["value"].(string))
			store[string(k)] = string(v)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case "/v3/kv/range":
			k, _ := base64.StdEncoding.DecodeString(body["key"].(string))
			keyStr := string(k)
			var kvs []map[string]any
			if _, ok := body["range_end"]; ok {
				for sk, sv := range store {
					kvs = append(kvs, map[string]any{
						"key":             base64.StdEncoding.EncodeToString([]byte(sk)),
						"value":           base64.StdEncoding.EncodeToString([]byte(sv)),
						"version":         "1",
						"create_revision": "1",
						"mod_revision":    "1",
					})
				}
			} else if sv, ok := store[keyStr]; ok {
				kvs = append(kvs, map[string]any{
					"key":             base64.StdEncoding.EncodeToString([]byte(keyStr)),
					"value":           base64.StdEncoding.EncodeToString([]byte(sv)),
					"version":         "1",
					"create_revision": "1",
					"mod_revision":    "1",
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"kvs": kvs})
		case "/v3/kv/deleterange":
			k, _ := base64.StdEncoding.DecodeString(body["key"].(string))
			delete(store, string(k))
			_ = json.NewEncoder(w).Encode(map[string]any{"deleted": "1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := Driver{}
	conn, err := d.Open(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Exec(context.Background(), `PUT /app/env "prod"`); err != nil {
		t.Fatalf("PUT failed: %v", err)
	}

	rows, err := conn.Query(context.Background(), `GET /app/env`)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	if !rows.Next() {
		t.Fatal("expected row for GET /app/env")
	}
	row, err := rows.Row()
	if err != nil {
		t.Fatalf("Row failed: %v", err)
	}
	if row.Values[1] != "prod" {
		t.Fatalf("expected value prod, got %v", row.Values[1])
	}
	_ = rows.Close()

	tables, err := d.Tables(context.Background(), conn)
	if err != nil {
		t.Fatalf("Tables failed: %v", err)
	}
	if len(tables) != 2 {
		t.Fatalf("expected 2 keys, got %v", tables)
	}
}
