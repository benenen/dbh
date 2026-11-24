package trino

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSyntaxAndSplit(t *testing.T) {
	d := Driver{}
	if !d.Syntax().FoldUnquotedIdentifiers {
		t.Fatal("expected FoldUnquotedIdentifiers")
	}
	schema, name := splitTable(`"my_schema"."events"`)
	if schema != "my_schema" || name != "events" {
		t.Fatalf("splitTable = (%q, %q)", schema, name)
	}
}

func TestOpenInvalidDSN(t *testing.T) {
	if _, err := (Driver{}).Open(context.Background(), "mysql://localhost/db", nil); err == nil {
		t.Fatal("expected error for non-trino DSN")
	}
}

func TestOpenWithDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not a real trino server", http.StatusBadRequest)
	}))
	defer srv.Close()

	var dialed atomic.Bool
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed.Store(true)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	_, err := (Driver{}).Open(context.Background(), srv.URL+"?user=alice", dial)
	if err == nil {
		t.Fatal("expected ping error from mock server")
	}
	if !dialed.Load() {
		t.Fatal("expected custom dialer to be invoked")
	}
}
