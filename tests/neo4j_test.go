//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestNeo4jE2E(t *testing.T) {
	if os.Getenv("DBH_TEST_NEO4J_DSN") == "" {
		t.Skip("DBH_TEST_NEO4J_DSN is not configured")
	}

	f := newFixture(t)
	f.run("new", "srv", "--driver", "neo4j", "--dsn-env", "DBH_TEST_NEO4J_DSN")
	contains(t, f.run("check", "srv"), "OK")

	t.Cleanup(func() {
		f.run("exec", "srv", `MATCH (n:DbhE2EItem) DETACH DELETE n`, "--no-history")
	})
	f.run("exec", "srv", `CREATE (n:DbhE2EItem {name: 'alpha', score: 1})`, "--no-history")
	contains(t, f.run("tables", "srv"), "DbhE2EItem")
	contains(t, f.run("describe", "srv", "DbhE2EItem"), "name")
	got := f.run("exec", "srv", `MATCH (n:DbhE2EItem) RETURN n.name AS name, n.score AS score`, "--format", "tsv")
	contains(t, got, "alpha\t1")
}

func TestNeo4jMockE2E(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Statements []struct {
				Statement string `json:"statement"`
			} `json:"statements"`
		}
		_ = json.Unmarshal(body, &req)
		stmt := ""
		if len(req.Statements) > 0 {
			stmt = req.Statements[0].Statement
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(stmt, "RETURN 1"):
			_, _ = io.WriteString(w, `{"results":[{"columns":["ok"],"data":[{"row":[1]}]}],"errors":[]}`)
		case strings.Contains(stmt, "db.labels"):
			_, _ = io.WriteString(w, `{"results":[{"columns":["label"],"data":[{"row":["Person"]}]}],"errors":[]}`)
		case strings.Contains(stmt, "UNWIND keys"):
			_, _ = io.WriteString(w, `{"results":[{"columns":["property"],"data":[{"row":["name"]}]}],"errors":[]}`)
		default:
			_, _ = io.WriteString(w, `{"results":[{"columns":["name"],"data":[{"row":["Alice"]}]}],"errors":[]}`)
		}
	}))
	defer srv.Close()

	f := newFixture(t)
	f.run("new", "nj", "--driver", "neo4j", "--dsn", srv.URL+"/neo4j")
	contains(t, f.run("check", "nj"), "OK")
	contains(t, f.run("tables", "nj"), "Person")
	contains(t, f.run("describe", "nj", "Person"), "name")
	contains(t, f.run("exec", "nj", `MATCH (n:Person) RETURN n.name AS name`, "--format", "tsv"), "Alice")
}
