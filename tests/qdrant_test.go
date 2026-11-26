//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestQdrantE2E(t *testing.T) {
	if os.Getenv("DBH_TEST_QDRANT_DSN") == "" {
		t.Skip("DBH_TEST_QDRANT_DSN is not configured")
	}

	f := newFixture(t)
	f.run("new", "srv", "--driver", "qdrant", "--dsn-env", "DBH_TEST_QDRANT_DSN")
	contains(t, f.run("check", "srv"), "OK")

	t.Cleanup(func() {
		f.run("exec", "srv", `DELETE /collections/dbh_e2e_items`, "--no-history")
	})
	f.run("exec", "srv", `PUT /collections/dbh_e2e_items {"vectors":{"size":4,"distance":"Cosine"}}`, "--no-history")
	contains(t, f.run("tables", "srv"), "dbh_e2e_items")
	contains(t, f.run("describe", "srv", "dbh_e2e_items"), "Cosine")
	contains(t, f.run("indexes", "srv", "dbh_e2e_items"), "hnsw")
	f.run("exec", "srv", `PUT /collections/dbh_e2e_items/points?wait=true {"points":[{"id":1,"vector":[0.1,0.2,0.3,0.4],"payload":{"tag":"go"}}]}`, "--no-history")
	got := f.run("exec", "srv", `POST /collections/dbh_e2e_items/points/scroll {"limit":10}`, "--format", "json")
	contains(t, got, `"go"`)
}

func TestQdrantMockE2E(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/collections":
			_, _ = io.WriteString(w, `{"result":{"collections":[{"name":"docs"}]},"status":"ok"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/collections/docs":
			_, _ = io.WriteString(w, `{"result":{"config":{"params":{"vectors":{"size":4,"distance":"Cosine"}}},"payload_schema":{}},"status":"ok"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/collections/docs/points/scroll":
			_, _ = io.WriteString(w, `{"result":{"points":[{"id":1,"payload":{"tag":"go"}}]},"status":"ok"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	f := newFixture(t)
	f.run("new", "qd", "--driver", "qdrant", "--dsn", srv.URL+"/docs")
	contains(t, f.run("check", "qd"), "OK")
	contains(t, f.run("tables", "qd"), "docs")
	contains(t, f.run("describe", "qd", "docs"), "Cosine")
	contains(t, f.run("indexes", "qd", "docs"), "hnsw")
	contains(t, f.run("exec", "qd", `POST points/scroll {"limit":5}`, "--format", "json"), `"go"`)
}
