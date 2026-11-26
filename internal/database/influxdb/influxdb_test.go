package influxdb

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/benenen/dbh/internal/database"
)

func TestInfluxDBDriver(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"pass"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/buckets":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"buckets":[{"name":"metrics"},{"name":"_monitoring"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/write":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/query":
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

	var dialed atomic.Bool
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed.Store(true)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}

	d := Driver{}
	ctx := context.Background()
	conn, err := d.Open(ctx, srv.URL+"/metrics?org=acme&token=tok", dial)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if !dialed.Load() || auth != "Token tok" {
		t.Fatalf("dialed=%v auth=%q", dialed.Load(), auth)
	}
	if dbs, err := d.Databases(ctx, conn); err != nil || len(dbs) != 1 || dbs[0] != "metrics" {
		t.Fatalf("Databases = %v, %v", dbs, err)
	}
	if tbls, err := d.Tables(ctx, conn); err != nil || len(tbls) != 1 || tbls[0] != "cpu" {
		t.Fatalf("Tables = %v, %v", tbls, err)
	}
	if cols, err := d.Columns(ctx, conn, "cpu"); err != nil || len(cols) != 1 || cols[0] != "usage" {
		t.Fatalf("Columns = %v, %v", cols, err)
	}
	if _, err := d.Indexes(ctx, conn, "cpu"); err != database.ErrNoIndexes {
		t.Fatalf("Indexes = %v", err)
	}
	if err := conn.Exec(ctx, "WRITE cpu,host=s1 usage=42.5"); err != nil {
		t.Fatalf("WRITE: %v", err)
	}
	rows, err := conn.Query(ctx, `from(bucket:"metrics") |> range(start: -1h)`)
	if err != nil || !rows.Next() {
		t.Fatalf("Flux Query: %v", err)
	}
	_ = rows.Close()
}
