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
		detailWant   string
		databases    string
		tables       string
		helpWant     string
		explainQuery string
	}{
		{
			driver:    "bbolt",
			tables:    "items",
			databases: "not supported",
			setup:     []string{"CREATE BUCKET items", "PUT items k1 v1"},
			query:     "GET items k1",
			want:      "v1",
			helpWant:  "bbolt accepts",
		},
		{
			driver:       "postgres",
			databases:    "dbh_test",
			envVar:       "DBH_TEST_POSTGRES_DSN",
			query:        "SELECT 1 AS val;",
			want:         "1",
			helpWant:     `\limit`,
			explainQuery: "SELECT 1",
		},
		{
			driver:       "mysql",
			databases:    "dbh_test",
			envVar:       "DBH_TEST_MYSQL_DSN",
			query:        "SELECT 1 AS val;",
			want:         "1",
			helpWant:     `\limit`,
			explainQuery: "SELECT 1",
		},
		{
			driver:    "mongo",
			databases: "admin",
			envVar:    "DBH_TEST_MONGO_DSN",
			query:     `{"ping":1}`,
			want:      "1",
			helpWant:  "MongoDB accepts",
		},
		{
			driver:    "redis",
			databases: "0",
			envVar:    "DBH_TEST_REDIS_DSN",
			query:     "PING",
			want:      "PONG",
			helpWant:  "Redis accepts",
		},
		{
			driver:       "clickhouse",
			databases:    "system",
			envVar:       "DBH_TEST_CLICKHOUSE_DSN",
			query:        "SELECT 1 AS val;",
			want:         "1",
			helpWant:     `\limit`,
			explainQuery: "SELECT 1",
		},
		{
			driver:    "mssql",
			databases: "master",
			envVar:    "DBH_TEST_MSSQL_DSN",
			query:     "SELECT 1 AS val;",
			want:      "1",
			helpWant:  `\limit`,
		},
		{
			driver:       "trino",
			databases:    "default",
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
			driver:    "oracle",
			databases: "SYSTEM",
			envVar:    "DBH_TEST_ORACLE_DSN",
			query:     "SELECT 1 AS val FROM DUAL;",
			want:      "1",
			helpWant:  `\limit`,
		},
		{
			driver:    "milvus",
			tables:    "dbh_term_items",
			databases: "default",
			envVar:    "DBH_TEST_MILVUS_DSN",
			setup:     []string{`POST collections/create {"collectionName":"dbh_term_items","dimension":4}`},
			cleanup:   []string{`POST collections/drop {"collectionName":"dbh_term_items"}`},
			query:     "POST collections/list {}",
			want:      "dbh_term_items",
			helpWant:  "Milvus accepts",
		},
		{
			driver:   "qdrant",
			envVar:   "DBH_TEST_QDRANT_DSN",
			query:    "GET /",
			want:     "qdrant",
			helpWant: "Qdrant accepts",
		},
		{
			driver:    "neo4j",
			databases: "neo4j",
			envVar:    "DBH_TEST_NEO4J_DSN",
			query:     "RETURN 1 AS val;",
			want:      "1",
			helpWant:  "Neo4j accepts",
		},
		{
			driver:    "influxdb",
			databases: "dbh_test",
			envVar:    "DBH_TEST_INFLUXDB_DSN",
			query:     `buckets() |> filter(fn: (r) => r.name == "dbh_test");`,
			want:      "dbh_test",
			helpWant:  "InfluxDB accepts",
		},
		{
			driver:    "tdengine",
			databases: "information_schema",
			envVar:    "DBH_TEST_TDENGINE_DSN",
			query:     "SELECT SERVER_VERSION();",
			want:      ".",
			helpWant:  `\limit`,
		},
		{
			driver:    "cassandra",
			databases: "system",
			envVar:    "DBH_TEST_CASSANDRA_DSN",
			query:     "SELECT release_version FROM system.local;",
			want:      ".",
			helpWant:  `\limit`,
		},
		{
			driver:    "etcd",
			databases: "not supported",
			envVar:    "DBH_TEST_ETCD_DSN",
			query:     "STATUS",
			want:      "3.5",
			helpWant:  "etcd accepts",
		},
		{
			driver:    "openobserve",
			tables:    "dbh_term",
			databases: "default",
			envVar:    "DBH_TEST_OPENOBSERVE_DSN",
			setup:     []string{`INGEST dbh_term {"msg":"ok"};`},
			query:     "SHOW STREAMS;",
			want:      "dbh_term",
			// Other e2e streams may sort before dbh_term, so the detail view only checks row 1's type.
			detailWant: "logs",
			helpWant:   "OpenObserve accepts",
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
			editorPath := filepath.Join(t.TempDir(), "editor.sh")
			writeFile(t, editorPath, "#!/bin/sh\nprintf '%s\\n' \"$DBH_TEST_EDITOR_QUERY\" > \"$1\"\ntouch \"$0.ran\"\n")
			if err := os.Chmod(editorPath, 0700); err != nil {
				t.Fatal(err)
			}
			f.env["VISUAL"] = editorPath
			f.env["DBH_TEST_EDITOR_QUERY"] = tc.query
			connName := "term-" + tc.driver
			f.run("new", connName, "--driver", tc.driver, "--dsn", dsn)
			if len(tc.cleanup) > 0 {
				t.Cleanup(func() {
					for _, stmt := range tc.cleanup {
						f.run("exec", connName, stmt, "--no-history")
					}
				})
			}

			if tc.databases == "not supported" {
				contains(t, f.fail("databases", connName), tc.databases)
			} else if tc.databases != "" {
				contains(t, f.run("databases", connName), tc.databases)
			}

			f.run("snippet", "set", "term_snip", tc.query)

			term := f.openTerminal(connName, "--no-history")
			contains(t, term.transcript, "Row limit: 1000")

			for _, stmt := range tc.setup {
				term.exchange(stmt+"\r", term.prompt)
			}

			term.exchange("\\refresh\r", term.prompt)
			if tc.databases != "" {
				contains(t, term.exchange("\\database\r", tc.databases), tc.databases)
			}
			if tc.tables != "" {
				contains(t, term.exchange("\\tables\r", tc.tables), tc.tables)
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

			detailWant := tc.want
			if tc.detailWant != "" {
				detailWant = tc.detailWant
			}
			detail := term.exchange("\r", "Row 1 (1/")
			contains(t, detail, detailWant)
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

			// The last typed query feeds \watch; Ctrl-C stops it and returns to the prompt.
			contains(t, term.exchange("\\watch 0.5\r", "Every 500ms"), "Every 500ms")
			term.exchange("\x03", "Watch stopped.")
			term.exchange("\r", term.prompt)

			term.exchange("\\x\r", "Expanded display is on.")
			contains(t, term.exchange(tc.query+"\r", term.prompt), tc.want)
			term.exchange("\\x\r", "Expanded display is off.")

			term.exchange("\\truncate 20\r", term.prompt)
			contains(t, term.exchange("\\truncate\r", "Truncate: 20"), "Truncate: 20")
			term.exchange("\\truncate off\r", term.prompt)
			contains(t, term.exchange("\\truncate\r", "Truncate: off"), "Truncate: off")

			scriptPath := filepath.Join(t.TempDir(), "script.sql")
			writeFile(t, scriptPath, tc.query+"\n")
			sourced := term.exchange("\\i "+scriptPath+"\r", term.prompt)
			contains(t, sourced, tc.want)
			excludes(t, sourced, "error:")

			ran := term.exchange("\\run term_snip\r", term.prompt)
			contains(t, ran, tc.want)
			excludes(t, ran, "error:")

			// VISUAL replaces the buffer with tc.query; Enter then runs the edited query.
			term.exchange("\\e\r", term.prompt)
			if _, err := os.Stat(editorPath + ".ran"); err != nil {
				t.Fatalf("editor did not run: %v", err)
			}
			edited := term.exchange("\r", term.prompt)
			contains(t, edited, tc.want)
			excludes(t, edited, "error:")

			if tc.explainQuery != "" {
				term.exchange("\\explain "+tc.explainQuery+"\r", term.prompt)
			}

			contains(t, term.exchange("\\reconnect\r", "Reconnected to "+connName), connName)
			term.exit("\\q\r")
		})
	}
}
