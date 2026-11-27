//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestOpenObserveMockE2E(t *testing.T) {
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
					{"level": "info", "msg": "ok"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	f := newFixture(t)
	f.run("new", "mock-o2", "--driver", "openobserve", "--dsn", srv.URL+"/default")
	f.run("check", "mock-o2")

	out := f.run("exec", "mock-o2", `INGEST app_logs {"level":"info","msg":"ok"}`, "--format", "tsv")
	contains(t, out, "app_logs")

	out = f.run("exec", "mock-o2", `SELECT level, msg FROM app_logs LIMIT 5`, "--format", "tsv")
	contains(t, out, "info")

	out = f.run("tables", "mock-o2")
	contains(t, out, "app_logs")

	out = f.run("describe", "mock-o2", "app_logs", "--format", "tsv")
	contains(t, out, "level")
}

func TestOpenObserveE2E(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("DBH_TEST_OPENOBSERVE_DSN"))
	if dsn == "" {
		t.Skip("DBH_TEST_OPENOBSERVE_DSN is not set")
	}

	f := newFixture(t)
	f.run("new", "live-o2", "--driver", "openobserve", "--dsn", dsn)
	f.run("check", "live-o2")

	out := f.run("exec", "live-o2", `INGEST dbh_e2e {"level":"info","msg":"hello_o2"}`, "--format", "tsv")
	contains(t, out, "dbh_e2e")

	out = f.run("exec", "live-o2", `SELECT level, msg FROM dbh_e2e LIMIT 5`, "--format", "tsv")
	contains(t, out, "hello_o2")

	contains(t, f.run("tables", "live-o2"), "dbh_e2e")
	contains(t, f.run("describe", "live-o2", "dbh_e2e", "--format", "tsv"), "level")
}
