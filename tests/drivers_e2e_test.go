//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestNewDriversE2E(t *testing.T) {
	for _, driver := range []string{"mssql", "oracle", "trino", "dm", "elasticsearch", "milvus"} {
		t.Run(driver, func(t *testing.T) {
			env := "DBH_TEST_" + strings.ToUpper(driver) + "_DSN"
			if os.Getenv(env) == "" {
				t.Skip(env + " is not configured")
			}

			f := newFixture(t)
			f.run("new", "srv", "--driver", driver, "--dsn-env", env)
			contains(t, f.run("check", "srv"), "OK")

			switch driver {
			case "mssql":
				dbArgs := []string{}
				if alt := os.Getenv("DBH_TEST_MSSQL_ALT_DATABASE"); alt != "" {
					dbArgs = append(dbArgs, "--db", alt)
				}
				t.Cleanup(func() {
					args := append([]string{"exec", "srv"}, dbArgs...)
					f.run(append(args, "DROP TABLE IF EXISTS dbo.dbh_e2e_items;", "--no-history")...)
				})
				args := append([]string{"exec", "srv"}, dbArgs...)
				f.run(append(args, "DROP TABLE IF EXISTS dbo.dbh_e2e_items; CREATE TABLE dbo.dbh_e2e_items (id INT PRIMARY KEY, name NVARCHAR(50)); INSERT INTO dbo.dbh_e2e_items VALUES (1, N'alpha');", "--no-history")...)
				got := f.run(append(args, "SELECT id, name FROM dbo.dbh_e2e_items;", "--format", "tsv")...)
				contains(t, got, "1\talpha")
				contains(t, f.run(append([]string{"tables", "srv"}, dbArgs...)...), "dbh_e2e_items")
				contains(t, f.run(append([]string{"describe", "srv"}, append(dbArgs, "dbo.dbh_e2e_items")...)...), "name")
				contains(t, f.run(append([]string{"indexes", "srv"}, append(dbArgs, "dbo.dbh_e2e_items")...)...), "id")

			case "oracle":
				t.Cleanup(func() {
					f.run("exec", "srv", "BEGIN EXECUTE IMMEDIATE 'DROP TABLE dbh_e2e_items'; EXCEPTION WHEN OTHERS THEN NULL; END; /", "--no-history")
				})
				f.run("exec", "srv", "CREATE TABLE dbh_e2e_items (id NUMBER PRIMARY KEY, name VARCHAR2(50)); INSERT INTO dbh_e2e_items VALUES (1, 'alpha'); COMMIT;", "--no-history")
				got := f.run("exec", "srv", "SELECT id, name FROM dbh_e2e_items", "--format", "tsv")
				contains(t, got, "1\talpha")
				contains(t, f.run("tables", "srv"), "DBH_E2E_ITEMS")
				contains(t, f.run("describe", "srv", "DBH_E2E_ITEMS"), "NAME")

			case "trino":
				t.Cleanup(func() {
					f.run("exec", "srv", "DROP TABLE IF EXISTS memory.default.dbh_e2e_items", "--no-history")
				})
				f.run("exec", "srv", "CREATE TABLE IF NOT EXISTS memory.default.dbh_e2e_items (id BIGINT, name VARCHAR); INSERT INTO memory.default.dbh_e2e_items VALUES (1, 'alpha');", "--no-history")
				got := f.run("exec", "srv", "SELECT id, name FROM memory.default.dbh_e2e_items", "--format", "tsv")
				contains(t, got, "1\talpha")
				contains(t, f.run("tables", "srv"), "dbh_e2e_items")
				contains(t, f.run("describe", "srv", "dbh_e2e_items"), "name")

			case "dm":
				t.Cleanup(func() {
					f.run("exec", "srv", "DROP TABLE IF EXISTS dbh_e2e_items;", "--no-history")
				})
				f.run("exec", "srv", "CREATE TABLE dbh_e2e_items (id INT PRIMARY KEY, name VARCHAR(50)); INSERT INTO dbh_e2e_items VALUES (1, 'alpha'); COMMIT;", "--no-history")
				got := f.run("exec", "srv", "SELECT id, name FROM dbh_e2e_items;", "--format", "tsv")
				contains(t, got, "1\talpha")

			case "elasticsearch":
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

			case "milvus":
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
		})
	}
}

func TestHTTPDriversMockE2E(t *testing.T) {
	t.Run("elasticsearch", func(t *testing.T) {
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
		f.run("new", "es", "--driver", "elasticsearch", "--dsn", srv.URL)
		contains(t, f.run("check", "es"), "OK")
		contains(t, f.run("tables", "es"), "users")
		contains(t, f.run("describe", "es", "users"), "name")
		contains(t, f.run("exec", "es", `POST /users/_search {"query":{"match_all":{}}}`, "--format", "json"), `"Alice"`)
	})

	t.Run("milvus", func(t *testing.T) {
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
	})
}
