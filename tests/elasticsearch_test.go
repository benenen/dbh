//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestElasticsearchE2E(t *testing.T) {
	if os.Getenv("DBH_TEST_ELASTICSEARCH_DSN") == "" {
		t.Skip("DBH_TEST_ELASTICSEARCH_DSN is not configured")
	}

	f := newFixture(t)
	f.run("new", "srv", "--driver", "elasticsearch", "--dsn-env", "DBH_TEST_ELASTICSEARCH_DSN")
	contains(t, f.run("check", "srv"), "OK")

	t.Cleanup(func() {
		f.run("exec", "srv", "DELETE /dbh_e2e_items", "--no-history")
	})
	f.run("exec", "srv", `PUT /dbh_e2e_items/_doc/1 {"name":"alpha","count":1}`, "--no-history")
	f.run("exec", "srv", `POST /dbh_e2e_items/_refresh`, "--no-history")
	contains(t, f.run("tables", "srv"), "dbh_e2e_items")
	contains(t, f.run("describe", "srv", "dbh_e2e_items"), "name")
	got := f.run("exec", "srv", `POST /dbh_e2e_items/_search {"query":{"match_all":{}}}`, "--format", "json")
	contains(t, got, `"alpha"`)
	sqlOut := f.run("exec", "srv", `POST /_sql {"query":"SELECT name FROM dbh_e2e_items"}`, "--format", "tsv")
	contains(t, sqlOut, "alpha")
}

func TestElasticsearchMockE2E(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			_, _ = io.WriteString(w, `{"name":"node-1","version":{"number":"8.15.0"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/_cat/indices":
			_, _ = io.WriteString(w, `[{"index":"users"}]`)
		case r.Method == http.MethodHead && r.URL.Path == "/users":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/users/_mapping":
			_, _ = io.WriteString(w, `{"users":{"mappings":{"properties":{"name":{"type":"text"}}}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/users/_search":
			_, _ = io.WriteString(w, `{"hits":{"hits":[{"_index":"users","_id":"1","_score":1.0,"_source":{"name":"Alice"}}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	f := newFixture(t)
	f.run("new", "es", "--driver", "elasticsearch", "--dsn", srv.URL, "--read-only")
	contains(t, f.run("check", "es"), "OK")
	contains(t, f.run("tables", "es"), "users")
	contains(t, f.run("describe", "es", "users"), "name")
	contains(t, f.run("exec", "es", `POST /users/_search {"query":{"match_all":{}}}`, "--format", "json"), `"Alice"`)
	contains(t, f.fail("exec", "es", `DELETE /users`), "read-only")
}
