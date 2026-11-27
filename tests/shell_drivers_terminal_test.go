//go:build e2e && (linux || darwin)

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalDriversInteractive(t *testing.T) {
	cases := []struct {
		driver       string
		envVar       string
		setup        []string
		cleanup      []string
		query        string
		want         string
		helpWant     string
		explainQuery string
	}{
		{
			driver:   "bbolt",
			setup:    []string{"CREATE BUCKET items", "PUT items k1 v1"},
			query:    "GET items k1",
			want:     "v1",
			helpWant: "bbolt accepts",
		},
		{
			driver:       "postgres",
			envVar:       "DBH_TEST_POSTGRES_DSN",
			query:        "SELECT 1 AS val;",
			want:         "1",
			helpWant:     `\limit`,
			explainQuery: "SELECT 1",
		},
		{
			driver:       "mysql",
			envVar:       "DBH_TEST_MYSQL_DSN",
			query:        "SELECT 1 AS val;",
			want:         "1",
			helpWant:     `\limit`,
			explainQuery: "SELECT 1",
		},
		{
			driver:   "mongo",
			envVar:   "DBH_TEST_MONGO_DSN",
			query:    `{"ping":1}`,
			want:     "1",
			helpWant: "MongoDB accepts",
		},
		{
			driver:   "redis",
			envVar:   "DBH_TEST_REDIS_DSN",
			query:    "PING",
			want:     "PONG",
			helpWant: "Redis accepts",
		},
		{
			driver:       "clickhouse",
			envVar:       "DBH_TEST_CLICKHOUSE_DSN",
			query:        "SELECT 1 AS val;",
			want:         "1",
			helpWant:     `\limit`,
			explainQuery: "SELECT 1",
		},
		{
			driver:   "mssql",
			envVar:   "DBH_TEST_MSSQL_DSN",
			query:    "SELECT 1 AS val;",
			want:     "1",
			helpWant: `\limit`,
		},
		{
			driver:       "trino",
			envVar:       "DBH_TEST_TRINO_DSN",
			query:        "SELECT 1 AS val;",
			want:         "1",
			helpWant:     `\limit`,
			explainQuery: "SELECT 1",
		},
		{
			driver:   "elasticsearch",
			envVar:   "DBH_TEST_ELASTICSEARCH_DSN",
			query:    "GET /",
			want:     "docker-cluster",
			helpWant: "Elasticsearch accepts",
		},
		{
			driver:   "oracle",
			envVar:   "DBH_TEST_ORACLE_DSN",
			query:    "SELECT 1 AS val FROM DUAL;",
			want:     "1",
			helpWant: `\limit`,
		},
		{
			driver:   "milvus",
			envVar:   "DBH_TEST_MILVUS_DSN",
			setup:    []string{`POST collections/create {"collectionName":"dbh_term_items","dimension":4}`},
			cleanup:  []string{`POST collections/drop {"collectionName":"dbh_term_items"}`},
			query:    "POST collections/list {}",
			want:     "dbh_term_items",
			helpWant: "Milvus accepts",
		},
		{
			driver:   "qdrant",
			envVar:   "DBH_TEST_QDRANT_DSN",
			query:    "GET /",
			want:     "qdrant",
			helpWant: "Qdrant accepts",
		},
		{
			driver:   "neo4j",
			envVar:   "DBH_TEST_NEO4J_DSN",
			query:    "RETURN 1 AS val;",
			want:     "1",
			helpWant: "Neo4j accepts",
		},
		{
			driver:   "influxdb",
			envVar:   "DBH_TEST_INFLUXDB_DSN",
			query:    `buckets() |> filter(fn: (r) => r.name == "dbh_test");`,
			want:     "dbh_test",
			helpWant: "InfluxDB accepts",
		},
		{
			driver:   "tdengine",
			envVar:   "DBH_TEST_TDENGINE_DSN",
			query:    "SELECT SERVER_VERSION();",
			want:     ".",
			helpWant: `\limit`,
		},
		{
			driver:   "cassandra",
			envVar:   "DBH_TEST_CASSANDRA_DSN",
			query:    "SELECT release_version FROM system.local;",
			want:     ".",
			helpWant: `\limit`,
		},
		{
			driver:   "etcd",
			envVar:   "DBH_TEST_ETCD_DSN",
			query:    "STATUS",
			want:     "3.5",
			helpWant: "etcd accepts",
		},
		{
			driver:   "openobserve",
			envVar:   "DBH_TEST_OPENOBSERVE_DSN",
			setup:    []string{`INGEST dbh_term {"msg":"ok"};`},
			query:    "SHOW STREAMS;",
			want:     "dbh_term",
			helpWant: "OpenObserve accepts",
		},
	}

	for _, tc := range cases {
		t.Run(tc.driver, func(t *testing.T) {
			dsn := ""
			if tc.envVar == "" {
				dsn = "bbolt://" + filepath.Join(t.TempDir(), "term.db")
			} else {
				dsn = strings.TrimSpace(os.Getenv(tc.envVar))
				if dsn == "" {
					t.Skipf("%s is not set", tc.envVar)
				}
			}

			f := newFixture(t)
			connName := "term-" + tc.driver
			f.run("new", connName, "--driver", tc.driver, "--dsn", dsn)
			if len(tc.cleanup) > 0 {
				t.Cleanup(func() {
					for _, stmt := range tc.cleanup {
						f.run("exec", connName, stmt, "--no-history")
					}
				})
			}

			term := f.openTerminal(connName, "--no-history")
			contains(t, term.transcript, "Row limit: 1000")

			for _, stmt := range tc.setup {
				term.exchange(stmt+"\r", term.prompt)
			}

			contains(t, term.exchange("\\help\r", tc.helpWant), tc.helpWant)
			contains(t, term.exchange("\\timeout 15s\r", "Timeout: 15s"), "15s")
			term.exchange("\\limit 5\r", term.prompt)
			contains(t, term.exchange("\\limit\r", "Limit: 5"), "Limit: 5")

			term.exchange("\\format table\r", term.prompt)
			term.exchange("\\pager on\r", term.prompt)

			pagerView := term.exchange(tc.query+"\r", "dbh results (")
			contains(t, pagerView, "No")
			contains(t, pagerView, tc.want)

			detail := term.exchange("\r", "Row 1 (1/")
			contains(t, detail, tc.want)
			term.exchange("\x1b", "dbh results (")
			term.exchange("q", term.prompt)

			term.exchange("\\pager off\r", term.prompt)
			outPath := filepath.Join(t.TempDir(), "out.txt")
			term.exchange("\\o "+outPath+"\r", "Output redirected")
			term.exchange(tc.query+"\r", term.prompt)
			term.exchange("\\o\r", "Output restored")
			rawOut, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatal(err)
			}
			contains(t, string(rawOut), tc.want)

			if tc.explainQuery != "" {
				term.exchange("\\explain "+tc.explainQuery+"\r", term.prompt)
			}

			contains(t, term.exchange("\\reconnect\r", "Reconnected to "+connName), connName)
			term.exit("\\q\r")
		})
	}
}
