//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestEtcdMockE2E(t *testing.T) {
	store := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/v3/maintenance/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version":   "3.5.17",
				"dbSize":    "20480",
				"leader":    "1",
				"raftIndex": "1",
				"raftTerm":  "1",
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

	f := newFixture(t)
	f.run("new", "mock-etcd", "--driver", "etcd", "--dsn", srv.URL)
	f.run("check", "mock-etcd")

	out := f.run("exec", "mock-etcd", `PUT /dbh/key "hello"`, "--format", "tsv")
	contains(t, out, "OK")

	out = f.run("exec", "mock-etcd", `GET /dbh/key`, "--format", "tsv")
	contains(t, out, "hello")

	out = f.run("tables", "mock-etcd")
	contains(t, out, "/dbh/key")

	out = f.run("exec", "mock-etcd", `DEL /dbh/key`, "--format", "tsv")
	contains(t, out, "1")
}

func TestEtcdE2E(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("DBH_TEST_ETCD_DSN"))
	if dsn == "" {
		t.Skip("DBH_TEST_ETCD_DSN is not set")
	}

	f := newFixture(t)
	f.run("new", "live-etcd", "--driver", "etcd", "--dsn", dsn)
	f.run("check", "live-etcd")

	out := f.run("exec", "live-etcd", `PUT /dbh_e2e/hello "world"`, "--format", "tsv")
	contains(t, out, "OK")

	out = f.run("exec", "live-etcd", `GET /dbh_e2e/hello`, "--format", "tsv")
	contains(t, out, "world")

	out = f.run("exec", "live-etcd", `DEL /dbh_e2e/hello`, "--format", "tsv")
	contains(t, out, "1")
}
