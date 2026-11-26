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

func TestInfluxDBE2E(t *testing.T) {
	if os.Getenv("DBH_TEST_INFLUXDB_DSN") == "" {
		t.Skip("DBH_TEST_INFLUXDB_DSN is not configured")
	}

	f := newFixture(t)
	f.run("new", "srv", "--driver", "influxdb", "--dsn-env", "DBH_TEST_INFLUXDB_DSN")
	contains(t, f.run("check", "srv"), "OK")

	f.run("exec", "srv", `WRITE dbh_e2e_cpu,host=ci usage=42.5`, "--no-history")
	contains(t, f.run("tables", "srv"), "dbh_e2e_cpu")
	contains(t, f.run("describe", "srv", "dbh_e2e_cpu"), "usage")
	got := f.run("exec", "srv", `from(bucket: "dbh_test") |> range(start: -1h) |> filter(fn: (r) => r._measurement == "dbh_e2e_cpu")`, "--format", "tsv")
	contains(t, got, "42.5")
}

func TestInfluxDBMockE2E(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"pass"}`)
		case r.URL.Path == "/api/v2/buckets":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"buckets":[{"name":"dbh_test"}]}`)
		case r.URL.Path == "/api/v2/write":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/v2/query":
			w.Header().Set("Content-Type", "text/csv")
			if strings.Contains(string(body), "schema.measurements") {
				_, _ = io.WriteString(w, ",result,table,_value\n,_result,0,cpu\n")
			} else if strings.Contains(string(body), "schema.measurementFieldKeys") {
				_, _ = io.WriteString(w, ",result,table,_value\n,_result,0,usage\n")
			} else {
				_, _ = io.WriteString(w, ",result,table,_measurement,_value\n,_result,0,cpu,42.5\n")
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	f := newFixture(t)
	f.run("new", "ifx", "--driver", "influxdb", "--dsn", srv.URL+"/dbh_test?org=acme&token=tok")
	contains(t, f.run("check", "ifx"), "OK")
	contains(t, f.run("tables", "ifx"), "cpu")
	contains(t, f.run("describe", "ifx", "cpu"), "usage")
	contains(t, f.run("exec", "ifx", `WRITE cpu,host=s1 usage=42.5`), "ok")
	contains(t, f.run("exec", "ifx", `from(bucket:"dbh_test") |> range(start:-1h)`, "--format", "tsv"), "42.5")
}
