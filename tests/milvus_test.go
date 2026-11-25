//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestMilvusE2E(t *testing.T) {
	if os.Getenv("DBH_TEST_MILVUS_DSN") == "" {
		t.Skip("DBH_TEST_MILVUS_DSN is not configured")
	}

	f := newFixture(t)
	f.run("new", "srv", "--driver", "milvus", "--dsn-env", "DBH_TEST_MILVUS_DSN")
	contains(t, f.run("check", "srv"), "OK")

	t.Cleanup(func() {
		f.run("exec", "srv", `POST collections/drop {"collectionName":"dbh_e2e_items"}`, "--no-history")
	})
	f.run("exec", "srv", `POST collections/create {"collectionName":"dbh_e2e_items","dimension":4}`, "--no-history")
	contains(t, f.run("tables", "srv"), "dbh_e2e_items")
	contains(t, f.run("describe", "srv", "dbh_e2e_items"), "vector")
	contains(t, f.run("indexes", "srv", "dbh_e2e_items"), "vector")
	f.run("exec", "srv", `POST entities/insert {"collectionName":"dbh_e2e_items","data":[{"id":1,"vector":[0.1,0.2,0.3,0.4]}]}`, "--no-history")
	got := f.run("exec", "srv", `POST entities/get {"collectionName":"dbh_e2e_items","id":[1]}`, "--format", "json")
	contains(t, got, `"id":1`)
}

func TestMilvusMockE2E(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/vectordb/databases/list":
			_, _ = io.WriteString(w, `{"code":0,"data":["default"]}`)
		case "/v2/vectordb/collections/list":
			_, _ = io.WriteString(w, `{"code":0,"data":["docs"]}`)
		case "/v2/vectordb/collections/describe":
			_, _ = io.WriteString(w, `{"code":0,"data":{"fields":[{"name":"id","type":"Int64","primaryKey":true},{"name":"vec","type":"FloatVector","params":[{"key":"dim","value":"4"}]}]}}`)
		case "/v2/vectordb/indexes/list":
			_, _ = io.WriteString(w, `{"code":0,"data":["idx_vec"]}`)
		case "/v2/vectordb/indexes/describe":
			_, _ = io.WriteString(w, `{"code":0,"data":[{"indexName":"idx_vec","fieldName":"vec","indexType":"AUTOINDEX","metricType":"COSINE"}]}`)
		case "/v2/vectordb/entities/query":
			_, _ = io.WriteString(w, `{"code":0,"data":[{"id":1,"title":"hello"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	f := newFixture(t)
	f.run("new", "mv", "--driver", "milvus", "--dsn", srv.URL+"/default")
	contains(t, f.run("check", "mv"), "OK")
	contains(t, f.run("tables", "mv"), "docs")
	contains(t, f.run("describe", "mv", "docs"), "vec")
	contains(t, f.run("indexes", "mv", "docs"), "COSINE")
	contains(t, f.run("exec", "mv", `POST entities/query {"collectionName":"docs","filter":"id > 0"}`, "--format", "tsv"), "1\thello")
}
